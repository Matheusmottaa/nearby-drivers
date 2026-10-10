FROM nginx:1.27-alpine

RUN rm -f /etc/nginx/conf.d/default.conf

COPY nginx.conf /etc/nginx/nginx.conf
COPY proxy-params.conf /etc/nginx/proxy-params.conf
COPY upstreams/ /etc/nginx/upstreams/
COPY refresh-upstreams.sh /usr/local/bin/refresh-upstreams.sh
COPY docker-entrypoint.sh /docker-entrypoint.sh

RUN chmod +x /usr/local/bin/refresh-upstreams.sh /docker-entrypoint.sh

EXPOSE 8080 8081
STOPSIGNAL SIGQUIT
ENTRYPOINT ["/docker-entrypoint.sh"]
