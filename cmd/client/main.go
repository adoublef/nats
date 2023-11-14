package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/rs/xid"
	lop "github.com/samber/lo/parallel"
	"golang.org/x/sync/errgroup"
)

//go:embed *.html
var embedFS embed.FS

type option struct {
	addr    string
	natsUrl string
}

func parse(fs *flag.FlagSet, args []string) (*option, error) {
	o := &option{}
	fs.StringVar(&o.addr, "addr", ":8080", "http listen address")
	fs.StringVar(&o.natsUrl, "nats", "nats://0.0.0.0:4222", "nats url")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("fs.Parse: %w", err)
	}
	return o, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fs := flag.NewFlagSet("", flag.ExitOnError)
	opts, err := parse(fs, os.Args[1:])
	if err != nil {
		log.Fatalln(err)
	}

	if err := run(ctx, opts); err != nil {
		log.Fatalln(err)
	}
}

func run(ctx context.Context, opts *option) error {
	nc, err := nats.Connect(opts.natsUrl)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	jsc, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("nc jetstream: %w", err)
	}
	kv, err := upsertKv(jsc, &nats.KeyValueConfig{
		Bucket:  "messages",
		TTL:     5 * time.Minute,
		Storage: nats.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("upsert kv: %w", err)
	}
	t, err := newTemplate(embedFS, "*.html")
	if err != nil {
		return err
	}
	s := newServerHTTP(opts.addr, t, kv)
	if err := newGroup(ctx,
		func(ctx context.Context) error {
			return s.ListenAndServe()
		},
		func(ctx context.Context) error {
			<-ctx.Done()
			return s.Shutdown(ctx)
		}).
		Wait(); err != nil {
		return fmt.Errorf("g.Wait: %w", err)
	}
	return nil
}

func newTemplate(fsys fs.FS, pattern ...string) (*template.Template, error) {
	var funcs = template.FuncMap{
		"env": func(s string) string {
			return os.Getenv(s)
		},
	}

	t, err := template.New("").Funcs(funcs).ParseFS(fsys, pattern...)
	if err != nil {
		return nil, fmt.Errorf("parse fs: %w", err)
	}
	return t, nil
}

func upsertKv(jsc nats.JetStreamContext, c *nats.KeyValueConfig) (nats.KeyValue, error) {
	if c == nil || c.Bucket == "" {
		return nil, errors.New("invalid config")
	}
	kv, err := jsc.KeyValue(c.Bucket)
	switch {
	case errors.Is(err, nats.ErrBucketNotFound):
		kv, err = jsc.CreateKeyValue(c)
		if err != nil {
			return nil, fmt.Errorf("create key value: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("key value: %w", err)
	}
	return kv, nil
}

func newServerHTTP(addr string, t *template.Template, kv nats.KeyValue) *http.Server {
	var (
		mux = chi.NewMux()
	)
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		mm := listMessages(kv)
		render(w, t, "index.html", mm)
	})
	mux.Post("/", func(w http.ResponseWriter, r *http.Request) {
		s := r.PostFormValue("message")
		err := addMessage(kv, s)
		if err != nil {
			http.Error(w, "Failed to add message", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	})
	return &http.Server{Addr: addr, Handler: mux}
}

func render(w http.ResponseWriter, t *template.Template, name string, data any) {
	err := t.ExecuteTemplate(w, name, data)
	if err != nil {
		http.Error(w, "failed to render", http.StatusInternalServerError)
		return
	}
}

type group struct {
	g   *errgroup.Group
	ctx context.Context
}

func newGroup(ctx context.Context, funcs ...func(context.Context) error) *group {
	g := &group{}
	g.g, g.ctx = errgroup.WithContext(ctx)
	g.Go(funcs...)
	return g
}

func (g *group) Go(funcs ...func(context.Context) error) {
	for _, f := range funcs {
		fn := f
		g.g.Go(func() error {
			return fn(g.ctx)
		})
	}
}

func (g *group) Wait() error {
	return g.g.Wait()
}

type message struct {
	ID      string
	Content string
	Region  string
}

func init() {
	gob.Register(message{})
}

func newMessage(content string) *message {
	return &message{
		ID:      xid.New().String(),
		Content: content,
		Region:  os.Getenv("FLY_REGION"),
	}
}

func addMessage(kv nats.KeyValue, content string) error {
	var (
		m   = newMessage(content)
		buf bytes.Buffer
	)

	err := gob.NewEncoder(&buf).Encode(m)
	if err != nil {
		return fmt.Errorf("gob encode: %w", err)
	}

	_, err = kv.Put(m.ID, buf.Bytes())
	if err != nil {
		return fmt.Errorf("kv put: %w", err)
	}
	return nil
}

func listMessages(kv nats.KeyValue) []*message {
	ee, err := kv.Keys()
	if err != nil && err != nats.ErrNoKeysFound {
		return nil
	}

	mm := lop.Map(ee, func(key string, i int) *message {
		m, err := getMessage(kv, key)
		if err != nil {
			log.Println(err)
			return nil
		}
		return m
	})

	return mm
}

func getMessage(kv nats.KeyValue, key string) (*message, error) {
	e, err := kv.Get(key)
	if err != nil {
		return nil, fmt.Errorf("kv get: %w", err)
	}
	var m message
	err = gob.NewDecoder(bytes.NewReader(e.Value())).Decode(&m)
	if err != nil {
		return nil, fmt.Errorf("gob decode: %w", err)
	}
	return &m, nil
}
