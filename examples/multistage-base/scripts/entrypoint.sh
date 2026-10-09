#!/bin/sh
# The ENTRYPOINT: prints what it runs as, then execs the CMD so the server is the main process.
echo "entrypoint: running as $(id -un) in $(pwd), version ${APP_VERSION}, argv: $*"
exec "$@"
