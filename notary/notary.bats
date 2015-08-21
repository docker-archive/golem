#!/usr/bin/env bats

# Registry host name, should be set to non-localhost address and match
# DNS name in nginx/ssl certificates and what is installed in /etc/docker/cert.d

load ../helpers

hostname=${TEST_REGISTRY:-"localregistry"}

repo=${TEST_REPO:-"hello-world"}
tag=${TEST_TAG:-"latest"}
image="${repo}:${tag}"

# Login information, should match values in nginx/test.passwd
user=${TEST_USER:-"testuser"}
password=${TEST_PASSWORD:-"passpassword"}
email="distribution@docker.com"

function trusted_run() {
	#DOCKER_CONTENT_TRUST_ROOT_PASSPHRASE="root5678" \
	#DOCKER_CONTENT_TRUST_SNAPSHOT_PASSPHRASE="snapshot" \
	#DOCKER_CONTENT_TRUST_TARGET_PASSPHRASE="target78" \
	#DOCKER_CONTENT_TRUST=1 DOCKER_CONTENT_TRUST_SERVER=https://$hostname:4443 \
	DOCKER_CONTENT_TRUST_OFFLINE_PASSPHRASE="offline8" \
	DOCKER_CONTENT_TRUST_TAGGING_PASSPHRASE="tagging8" \
	DOCKER_CONTENT_TRUST=1 \
	run $@
}

function setup() {
	if [ "$TEST_SKIP_PULL" == "" ]; then
		docker pull $image
	fi
}

@test "Test valid certificates (trusted)" {
	# Skip in less than 1.8
	docker tag -f $image $hostname:5440/$image
	trusted_run docker -D push $hostname:5440/$image
	[ "$status" -eq 0 ]
	has_digest "$output"
}

@test "Test basic auth (trusted)" {
	login $hostname:5441
	docker tag -f $image $hostname:5441/$image
	trusted_run docker push $hostname:5441/$image
	[ "$status" -eq 0 ]
	has_digest "$output"
}

@test "Test TLS client auth (trusted)" {
	docker tag -f $image $hostname:5442/$image
	trusted_run docker push $hostname:5442/$image
	[ "$status" -eq 0 ]
	has_digest "$output"
}

@test "Test TLS client with invalid certificate authority fails (trusted)" {
	docker tag -f $image $hostname:5440/$image
	DOCKER_CONTENT_TRUST_SERVER=https://$hostname:5443 trusted_run docker push $hostname:5440/$image
	[ "$status" -ne 0 ]
}

@test "Test basic auth with TLS client auth (trusted)" {
	login $hostname:5444
	docker tag -f $image $hostname:5444/$image
	trusted_run docker push $hostname:5444/$image
	[ "$status" -eq 0 ]
	has_digest "$output"
}

@test "Test unknown certificate authority fails (trusted)" {
	docker tag -f $image $hostname:5440/$image
	DOCKER_CONTENT_TRUST_SERVER=https://$hostname:5445 trusted_run docker push $hostname:5440/$image
	[ "$status" -ne 0 ]
}

@test "Test basic auth with unknown certificate authority fails (trusted)" {
	run login $hostname:5446
	[ "$status" -ne 0 ]
	docker tag -f $image $hostname:5446/$image
	trusted_run docker push $hostname:5446/$image
	[ "$status" -ne 0 ]
}

@test "Test TLS client auth to server with unknown certificate authority fails (trusted)" {
	docker tag -f $image $hostname:5440/$image
	DOCKER_CONTENT_TRUST_SERVER=https://$hostname:5447 trusted_run docker push $hostname:5440/$image
	[ "$status" -ne 0 ]
}

@test "Test failure to connect to server fails to fallback to SSLv3 (trusted)" {
	docker tag -f $image $hostname:5440/$image
	DOCKER_CONTENT_TRUST_SERVER=https://$hostname:5448 trusted_run docker push $hostname:5440/$image
	[ "$status" -ne 0 ]
}

