#!/bin/sh
set -eu

docker compose up --build --wait
printf '%s\n' 'ContentFlow is ready at http://127.0.0.1:3100'
