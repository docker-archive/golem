# golem is an function to run tests from a local gopath
# The local gopath should have docker and golem checked out
function golem() {
  $GOPATH/src/github.com/docker/golem/run.sh $@
}

# golem-docker is a function to run tests for a Docker development build
# The first argument is docker binary bundle version to run ("default" to use image's default)
# First issue "make binary" for the version to run
function golem-docker() {
  if [ "$1" != "default" ]; then
    DOCKER_BINARY=$GOPATH/src/github.com/docker/docker/bundles/$1/binary/docker
    if [ ! -f $DOCKER_BINARY ]; then
      current_version=`cat $GOPATH/src/github.com/docker/docker/VERSION`
      echo "$DOCKER_BINARY does not exist"
      echo "Current checked out docker version: $current_version"
      echo "Checkout desired version and run 'make binary' from $GOPATH/src/github.com/docker/docker"
      return 1
    fi
  fi
  shift

  DOCKER_BINARY="$DOCKER_BINARY" $GOPATH/src/github.com/docker/golem/run.sh $@
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
  golem-docker $version $@
}

