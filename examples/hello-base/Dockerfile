# An example build context for `zbox base build`: run it from this directory, or `zbox base build examples/hello-base --name hello`.
# The image becomes a base, `zbox new --base hello` boots it, and the server answers on port 8080 (`zbox port open <id> 8080`).
FROM alpine:3.20

RUN apk add --no-cache python3 curl

COPY app/server.py /usr/local/bin/server
RUN chmod 0755 /usr/local/bin/server

ENV GREETING="hello from a built base"
WORKDIR /workspace
EXPOSE 8080

CMD ["server"]
