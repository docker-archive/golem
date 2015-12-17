#!/usr/bin/env bats

# This tests various expected error scenarios when pulling bad content

load ../helpers

host="localregistry:6666"
base="hello-world"

function setup() {
	if [ "$TEST_SKIP_PULL" == "" ]; then
		docker pull $base:latest
	fi

}

@test "Test malevolent proxy pass through" {
	docker tag -f $base:latest $host/$base/nochange:latest
	run docker push $host/$base/nochange:latest
	[ "$status" -eq 0 ]
	has_digest "$output"

	run docker pull $host/$base/nochange:latest
	echo "$output"
	[ "$status" -eq 0 ]
}

@test "Test malevolent image name change" {
	imagename="$host/$base/rename"
	image="$imagename:lastest"
	docker tag -f $base:latest $image
	run docker push $image
	[ "$status" -eq 0 ]
	has_digest "$output"

	# Pull attempt should fail to verify manifest digest
	run docker pull "$imagename@$digest"
	echo "$output"
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

@test "Test malevolent poisoned images" {
        truncid="777cf9284131"
	poison="${truncid}d77ca0863fb7f054c0a276d7e227b5e9a5d62b497979a481fa32"
	image1="$host/$base/image1/poison:$poison"
	tempImage $image1
	run docker push $image1
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"

	image2="$host/$base/image2/poison:$poison"
	tempImage $image2
	run docker push $image2
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"


	# Remove image to ensure layer is pulled and digest verified
	docker rmi -f $image1
	docker rmi -f $image2

	run docker pull $image1
	echo "$output"
	[ "$status" -eq 0 ]
	run docker pull $image2
	echo "$output"
	[ "$status" -eq 0 ]

	# Test if there are multiple images
	run docker images
	echo "$output"
	[ "$status" -eq 0 ]

	# Test images have same ID and not the poison
	id1=$(docker inspect --format="{{.Id}}" $image1)
	id2=$(docker inspect --format="{{.Id}}" $image2)

	# Remove old images
	docker rmi -f $image1
	docker rmi -f $image2

	[ "$id1" != "$id2" ]

	[ "$id1" != "$truncid" ]

	[ "$id2" != "$truncid" ]
}

@test "Test malevolent altered identical images" {
        truncid1="777cf9284131"
	poison1="${truncid1}d77ca0863fb7f054c0a276d7e227b5e9a5d62b497979a481fa32"
        truncid2="888cf9284131"
	poison2="${truncid2}d77ca0863fb7f054c0a276d7e227b5e9a5d62b497979a481fa64"

	image1="$host/$base/image1/alteredid:$poison1"
	tempImage $image1
	run docker push $image1
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"

	image2="$host/$base/image2/alteredid:$poison2"
	docker tag -f $image1 $image2
	run docker push $image2
	echo "$output"
	[ "$status" -eq 0 ]
	has_digest "$output"


	# Remove image to ensure layer is pulled and digest verified
	docker rmi -f $image1
	docker rmi -f $image2

	run docker pull $image1
	echo "$output"
	[ "$status" -eq 0 ]
	run docker pull $image2
	echo "$output"
	[ "$status" -eq 0 ]

	# Test if there are multiple images
	run docker images
	echo "$output"
	[ "$status" -eq 0 ]

	# Test images have same ID and not the poison
	id1=$(docker inspect --format="{{.Id}}" $image1)
	id2=$(docker inspect --format="{{.Id}}" $image2)

	# Remove old images
	docker rmi -f $image1
	docker rmi -f $image2

	[ "$id1" == "$id2" ]

	[ "$id1" != "$truncid1" ]

	[ "$id2" != "$truncid2" ]
}
