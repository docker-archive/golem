#!/usr/bin/env bash
set -e

cd "$(dirname "$(readlink -f "$BASH_SOURCE")")"

function execute() {
	>&2 echo "++ $@"
	eval "$@"
}

execute time docker-compose build

execute docker-compose up -d
trap "docker-compose stop &> /dev/null" EXIT

execute docker-compose logs > /var/log/compose.log &

# Setup test environment
export TEST_REPO="hello-world"
export TEST_TAG="latest"
export TEST_USER="testuser"
export TEST_PASSWORD="passpassword"
export TEST_REGISTRY="localregistry"

# Pull images used for tests
docker pull "${TEST_REPO}:${TEST_TAG}"
export TEST_SKIP_PULL="true"

# Run the tests.
execute time bats -p $@
