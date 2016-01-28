#!/usr/bin/env bats

# Tests pushing and pulling with the v1 registry

load helpers

hostname=${TEST_REGISTRY:-"localregistry"}
host="$hostname:5011"

repo="hello-world"
tag="latest"
image="${repo}:${tag}"

function setup() {
	if [ "$TEST_SKIP_PULL" == "" ]; then
		docker pull $image
	fi
}

function createAndPushImage() {
	build $1 $image
	docker push $1
	docker rmi $1
}

@test "Test v1 push and pull" {
	imagename=$host/testv1push
	run createAndPushImage $imagename
	echo $output
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

@test "Test v1 run after pull" {
	imagename=$host/testv1run
	run createAndPushImage $imagename
	echo $output
	[ "$status" -eq 0 ]

	run docker pull $imagename
	echo $output
	[ "$status" -eq 0 ]

	run docker images --no-trunc
	echo "$output"
	[ "$status" -eq 0 ]
	
	run docker run $imagename
	[ "$status" -eq 0 ]

	trimmed=$(echo -e "${output}" | cut -c1-17 | head -n 2 | tail -n 1)
	echo "\"$trimmed\""
	teststr="Hello from Docker"
	[ "$trimmed" == "$teststr" ]
}

