# syntax=docker/dockerfile:1

FROM nginx:alpine AS runtime

COPY ./etc/nginx.conf /etc/nginx/nginx.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]