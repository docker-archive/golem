#!/usr/bin/env bats

# Tests pushing and pulling with the v1 registry

load ../helpers

hostname=${TEST_REGISTRY:-"localregistry"}
host="$hostname:5011"

repo=${TEST_REPO:-"hello-world"}
tag=${TEST_TAG:-"latest"}
image="${repo}:${tag}"

function setup() {
	if [ "$TEST_SKIP_PULL" == "" ]; then
		docker pull $image
	fi
}

function build() {
	docker build --no-cache -t $1 - <<DOCKERFILE
FROM $image
MAINTAINER derek@docker.com
DOCKERFILE
}

@test "Test v1 push and pull" {
	imagename=$host/testv1push
	run build $imagename
	[ "$status" -eq 0 ]

	run docker push $imagename
	echo $output
	[ "$status" -eq 0 ]

	# Remove imagename
	run docker rmi $imagename
	[ "$status" -eq 0 ]

	run docker pull $imagename
	echo $output
	[ "$status" -eq 0 ]

	imageid1=$(docker images -q --no-trunc $imagename)

	run docker rmi $imagename
	[ "$status" -eq 0 ]

	run docker pull $imagename
	[ "$status" -eq 0 ]

	imageid2=$(docker images -q --no-trunc $imagename)
	[ "$imageid1" == "$imageid2" ]
}


