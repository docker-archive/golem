#!/usr/bin/env bats

# Tests pushing and pulling with the v1 registry

load helpers

hostname=${TEST_REGISTRY:-"localregistry"}
host="$hostname:5011"

function createAndPushImage() {
	helloImage $1
	docker_t push $1
	docker_t rmi $1
}

@test "Test v1 push and pull" {
	imagename=$host/testv1push
	run createAndPushImage $imagename
	echo $output
	[ "$status" -eq 0 ]

	run docker_t pull $imagename
	echo $output
	[ "$status" -eq 0 ]

	imageid1=$(docker_t images -q --no-trunc $imagename)

	run docker_t rmi $imagename
	[ "$status" -eq 0 ]

	run docker_t pull $imagename
	[ "$status" -eq 0 ]

	imageid2=$(docker_t images -q --no-trunc $imagename)
	[ "$imageid1" == "$imageid2" ]
}

@test "Test v1 run after pull" {
	imagename=$host/testv1run
	run createAndPushImage $imagename
	echo $output
	[ "$status" -eq 0 ]

	run docker_t pull $imagename
	echo $output
	[ "$status" -eq 0 ]

	run docker_t images --no-trunc
	echo "$output"
	[ "$status" -eq 0 ]
	
	run docker_t run $imagename
	[ "$status" -eq 0 ]

	trimmed=$(echo -e "${output}" | cut -c1-17 | head -n 2 | tail -n 1)
	echo "\"$trimmed\""
	teststr="Hello Golem!"
	[ "$trimmed" == "$teststr" ]
}

