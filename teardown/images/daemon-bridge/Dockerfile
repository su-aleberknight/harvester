FROM alpine:3.18

RUN apk add --no-cache iproute2 bash

COPY create-bridge.sh /usr/local/bin/create-bridge.sh
RUN chmod +x /usr/local/bin/create-bridge.sh

ENTRYPOINT ["/usr/local/bin/create-bridge.sh"]