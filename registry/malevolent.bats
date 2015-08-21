#!/usr/bin/env bats

# This tests various expected error scenarios when pulling bad content

load ../helpers

host="localregistry:6666"
base="hello-world"

function setup() {
	docker pull $base:latest
}

@test "Test malevolent proxy pass through" {
	docker tag -f $base:latest $host/$base/nochange:latest
	run docker push $host/$base/nochange:latest
	[ "$status" -eq 0 ]
	has_digest "$output"

	run docker pull $host/$base/nochange:latest
	[ "$status" -eq 0 ]
}

@test "Test malevolent bad signature change" {
	docker tag -f $base:latest $host/$base/badsignature:latest
	run docker push $host/$base/badsignature:latest
	[ "$status" -eq 0 ]
	has_digest "$output"

	run docker pull $host/$base/badsignature:latest
	[ "$status" -ne 0 ]
}

@test "Test malevolent image name change" {
	imagename="$host/$base/rename"
	image="$imagename:lastest"
	docker tag -f $base:latest $image
	run docker push $image
	[ "$status" -eq 0 ]
	has_digest "$output"

	# Pull attempt should fail to verify manifest digest
	run docker pull "$image@$digest"
	[ "$status" -ne 0 ]
}

@test "Test malevolent altered layer" {
	image="$host/$base/addfile:latest"
	tempImage $image
	run docker push $image
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"

	# Remove image to ensure layer is pulled and digest verified
	docker rmi -f $image

	run docker pull $image
	echo "$output"
	[ "$status" -ne 0 ]
}

@test "Test malevolent altered layer (by digest)" {
	imagename="$host/$base/addfile"
	image="$imagename:latest"
	tempImage $image
	run docker push $image
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"

	# Remove image to ensure layer is pulled and digest verified
	docker rmi -f $image

	run docker pull "$imagename@$digest"
	echo "$output"
	[ "$status" -ne 0 ]
}

