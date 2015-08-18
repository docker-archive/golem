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
