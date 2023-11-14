# nats

## Embed

To run the embedded project use the following command

```bash
docker-compose -f docker-compose.embed.yml up -d --build --force-recreate
```

To stop the containers use the following

```bash
docker-compose -f {{.CONFIG}} down --volumes
```

If you have _Taskfile_ installed then you can use the following commands instead

```bash
task start CONFIG=docker-compose.embed.yml
task stop CONFIG=docker-compose.embed.yml
```
