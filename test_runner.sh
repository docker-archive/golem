#!/usr/bin/env bash
set -e

cd "$(dirname "$(readlink -f "$BASH_SOURCE")")"

function execute() {
	>&2 echo "++ $@"
	eval "$@"
}

execute time docker-compose build

execute docker-compose up -d

# Run the tests.
execute time bats -p $@
