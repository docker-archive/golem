# TODO: Set docker development root (default to $GOROOT/src/github.com/docker/docker
# TODO: Support $GOPATH with multiple entries, use first by default

# golem-docker is a function to run tests for a Docker development build
# The first argument is docker binary bundle version to run ("default" to use image's default)
# First issue "make binary" for the version to run
function golem-docker() {
  docker_args=""
  if [[ "$1" == *"-dev" ]]; then
    DOCKER_BINARY=$(readlink -f "$GOPATH/src/github.com/docker/docker/bundles/$1/binary/docker")
    if [ ! -f $DOCKER_BINARY ]; then
      current_version=`cat $GOPATH/src/github.com/docker/docker/VERSION`
      echo "$DOCKER_BINARY does not exist"
      echo "Current checked out docker version: $current_version"
      echo "Checkout desired version and run 'make binary' from $GOPATH/src/github.com/docker/docker"
      return 1
    fi
    docker_args="-db=$DOCKER_BINARY"
  elif [ "$1" != "default" ]; then
    docker_args="-docker-version=$1"
  fi
  shift

  golem $docker_args $@
}

function path_save_cd() {
	export GOLEM_SAVED_PATH="$(pwd)"
	cd $1
}

function path_restore() {
	if [ "$GOLEM_SAVED_PATH" != "" ]; then
		cd $GOLEM_SAVED_PATH
		unset GOLEM_SAVED_PATH
	fi
}

# golem-docker-dev is a function to run tests on active docker development directory
function golem-docker-dev() {
  path_save_cd $GOPATH/src/github.com/docker/docker/
  trap path_restore EXIT
  make binary
  version=`cat VERSION`
  path_restore

  binary=$(readlink -f "$GOPATH/src/github.com/docker/docker/bundles/$version/binary/docker")
  if [ ! -f $binary ]; then
    echo "Failed to get binary for $version"
    return 1
  fi

  golem -db $binary $@
}

