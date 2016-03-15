#!/usr/bin/env bats

# This tests contacting a registry using a token server

load helpers

user="testuser"
password="testpassword"
email="a@nowhere.com"
base="hello-world"

@test "Test token server login" {
	run docker login -u $user -p $password -e $email localregistry:5554
	echo $output
	[ "$status" -eq 0 ]

	# First line is WARNING about credential save or email deprecation
	[ "${lines[2]}" = "Login Succeeded" -o "${lines[1]}" = "Login Succeeded" ]
}

@test "Test token server bad login" {
	run docker login -u "testuser" -p "badpassword" -e $email localregistry:5554
	[ "$status" -ne 0 ]

	run docker login -u "baduser" -p "testpassword" -e $email localregistry:5554
	[ "$status" -ne 0 ]
}

@test "Test push and pull with token auth" {
	login localregistry:5555
	image="localregistry:5555/testuser/token"
	build $image "$base:latest"

	run docker push $image
	echo $output
	[ "$status" -eq 0 ]

	docker rmi $image

	run docker pull $image
	[ "$status" -eq 0 ]
}

@test "Test push and pull with token auth wrong namespace" {
	login localregistry:5555
	image="localregistry:5555/notuser/token"
	build $image "$base:latest"

	run docker push $image
	[ "$status" -ne 0 ]
}

@test "Test oauth token server login" {
	version_check docker "$DOCKER_VERSION" "1.11.0"

	login_oauth localregistry:5557
}

@test "Test oauth token server bad login" {
	version_check docker "$DOCKER_VERSION" "1.11.0"

	run docker login -u "testuser" -p "badpassword" -e $email localregistry:5557
	[ "$status" -ne 0 ]

	run docker login -u "baduser" -p "testpassword" -e $email localregistry:5557
	[ "$status" -ne 0 ]
}

@test "Test oauth push and pull with token auth" {
	version_check docker "$DOCKER_VERSION" "1.11.0"

	login_oauth localregistry:5558
	image="localregistry:5558/testuser/token"
	build $image "$base:latest"

	run docker push $image
	echo $output
	[ "$status" -eq 0 ]

	docker rmi $image

	run docker pull $image
	[ "$status" -eq 0 ]
}

@test "Test oauth push and pull with token auth wrong namespace" {
	version_check docker "$DOCKER_VERSION" "1.11.0"

	login_oauth localregistry:5558
	image="localregistry:5558/notuser/token"
	build $image "$base:latest"

	run docker push $image
	[ "$status" -ne 0 ]
}
