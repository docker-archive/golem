FROM alpine:3.3

RUN apk add --no-cache \
                bash \
                btrfs-progs \
                curl \
                e2fsprogs \
                iptables \
                xz

ENV DOCKER_BUCKET get.docker.com
ENV DOCKER_VERSION 1.10.2
ENV DOCKER_SHA256 3fcac4f30e1c1a346c52ba33104175ae4ccbd9b9dbb947f56a0a32c9e401b768

RUN curl -fSL "https://${DOCKER_BUCKET}/builds/Linux/x86_64/docker-$DOCKER_VERSION" -o /usr/local/bin/docker \
	&& echo "${DOCKER_SHA256}  /usr/local/bin/docker" | sha256sum -c - \
	&& chmod +x /usr/local/bin/docker

ENV DIND_COMMIT 3b5fac462d21ca164b3778647420016315289034

RUN curl -fSL "https://raw.githubusercontent.com/docker/docker/3b5fac462d21ca164b3778647420016315289034/hack/dind" -o /usr/local/bin/dind \
	&& chmod +x /usr/local/bin/dind

# Install golem binary
COPY golem /usr/local/bin/golem

VOLUME /var/lib/docker

ENTRYPOINT [ "/usr/local/bin/dind" ]
CMD [ "golem" ]
