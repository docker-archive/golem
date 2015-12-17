#!/usr/bin/env bash
set -e
set -x

cd "$(dirname "$(readlink -f "$BASH_SOURCE")")"
TEST_ROOT=$(pwd -P)

source helpers.bash

volumeMount=""
if [ "$DOCKER_VOLUME" != "" ]; then
	volumeMount="-v ${DOCKER_VOLUME}:/var/lib/docker"
fi

dockerMount=""
if [ "$DOCKER_BINARY" != "" ]; then
	dockerMount="-v ${DOCKER_BINARY}:/usr/local/bin/docker"
else
	DOCKER_BINARY=docker
fi

DISTRIBUTION_IMAGE=${DISTRIBUTION_IMAGE:-registry:2.1.1}
distributionMount=""
if [ "$DISTRIBUTION_BUILD_DIR" != "" ]; then
	distributionMount="-v ${DISTRIBUTION_BUILD_DIR}:/build/distribution"
	DISTRIBUTION_IMAGE=""
fi

logMount=""
if [ "$TEST_LOG_DIR" != "" ]; then
	logMount="-v ${TEST_LOG_DIR}:/var/log"
fi

# Image containing the integration tests environment.
image="$INTEGRATION_IMAGE"
if [ "$image" == "" ]; then
	image="distribution/docker-integration:latest"
	docker pull $image
fi

if [ "$1" == "-d" ]; then
	start_daemon
	shift
fi

TESTS=${@:-registry}

# Start a Docker engine inside a docker container
ID=$(docker run -d -it --privileged $volumeMount $dockerMount $logMount \
	$distributionMount \
	-v ${TEST_ROOT}:/runner \
	-w /runner \
	-e "DOCKER_GRAPHDRIVER=$DOCKER_GRAPHDRIVER" \
	-e "EXEC_DRIVER=$EXEC_DRIVER" \
	${image} \
	./run_engine.sh)

# Stop container on exit
trap "docker rm -f -v $ID" EXIT

# Wait for it to become reachable.
tries=10
until docker exec "$ID" docker version &> /dev/null; do
	(( tries-- ))
	if [ $tries -le 0 ]; then
		echo >&2 "error: daemon failed to start"
		exit 1
	fi
	sleep 1
done

# If no volume is specified, transfer images into the container from
# the outer docker instance
if [ "$DOCKER_VOLUME" == "" ]; then
	# Make sure we have images outside the container, to transfer to the container.
	# Not much will happen here if the images are already present.
	docker build -t runner_nginx ./nginx
	docker pull registry:0.9.1

	load_image docker "$DISTRIBUTION_IMAGE" golem/distribution:latest "$DISTRIBUTION_BUILD_DIR"

	# Transfer images to the inner container.
	for image in registry:0.9.1 golem/distribution:latest runner_nginx; do
		docker save "$image" | docker exec -i "$ID" docker load
	done
else
	load_image "docker exec -i $ID docker" "$DISTRIBUTION_IMAGE" golem/distribution:latest /build/distribution
fi

# Run the tests.
docker exec -it "$ID" sh -c "./test_runner.sh $TESTS"

