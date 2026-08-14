#!/bin/sh
set -eu

docker compose up --build --wait
printf '%s\n' 'ContentFlow is ready at http://localhost:3000'
