# Start docker daemon
function start_daemon() {
	# Drivers to use for Docker engines the tests are going to create.
	STORAGE_DRIVER=${STORAGE_DRIVER:-overlay}
	EXEC_DRIVER=${EXEC_DRIVER:-native}

	docker --daemon --log-level=panic \
		--storage-driver="$STORAGE_DRIVER" --exec-driver="$EXEC_DRIVER" &
	DOCKER_PID=$!

	# Wait for it to become reachable.
	tries=10
	until docker version &> /dev/null; do
		(( tries-- ))
		if [ $tries -le 0 ]; then
			echo >&2 "error: daemon failed to start"
			exit 1
		fi
		sleep 1
	done
}

#load_image build or pulls an image
function load_image() {
	docker_command="$1"
	image_name="$2"
	remote_image="$3"
	build_dir="$4"
	build_flags="$5"
	if [ "$image_name" == "" ]; then
		$docker_command build $build_flags -t "$remote_image" "$build_dir"
	else
		$docker_command pull "$image_name"
		$docker_command tag -f "$image_name" "$remote_image"
	fi

}

# has_digest enforces the last output line is "Digest: sha256:..."
# the input is the output from a docker push cli command
function has_digest() {
	filtered=$(echo "$1" |sed -rn '/[dD]igest\: sha(256|384|512)/ p')
	[ "$filtered" != "" ]
	digest=$(expr "$filtered" : ".*\(sha\(256\|384\|512\):[a-z0-9]*\)")
}

# tempImage creates a new image using the provided name
# requires bats
function tempImage() {
	dir=$(mktemp -d)
	run dd if=/dev/random of="$dir/f" bs=1024 count=0 seek=16
	cat <<DockerFileContent > "$dir/Dockerfile"
FROM scratch
COPY f /f

CMD []
DockerFileContent
	docker build --no-cache -t $1 $dir
}

# skip basic auth tests with Docker 1.6, where they don't pass due to
# certificate issues, requires bats
function basic_auth_version_check() {
	run sh -c 'docker version | fgrep -q "Client version: 1.6."'
	if [ "$status" -eq 0 ]; then
		skip "Basic auth tests don't support 1.6.x"
	fi
}

# login issues a login to docker to the provided server
# uses user, password, and email variables set outside of function
# requies bats
function login() {
	run docker login -u $user -p $password -e $email $1
	if [ "$status" -ne 0 ]; then
		echo $output
	fi
	[ "$status" -eq 0 ]
	# First line is WARNING about credential save
	[ "${lines[1]}" = "Login Succeeded" ]
}

# build reates a new docker image id from another image
function build() {
	docker build --no-cache -t $1 - <<DOCKERFILE
FROM $2
MAINTAINER distribution@docker.com
DOCKERFILE
}
