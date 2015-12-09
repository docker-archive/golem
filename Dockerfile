FROM debian:jessie

MAINTAINER Docker Distribution Team <distribution@docker.com>

# compile and runtime deps
# https://github.com/docker/docker/blob/master/project/PACKAGERS.md#runtime-dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
        # For DIND
        ca-certificates \
        curl \
        iptables \
        procps \
        e2fsprogs \
        xz-utils \
        # For build
        build-essential \
        file \
        git \
        net-tools \ 
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# Install DIND
RUN curl -L -o /dind https://raw.githubusercontent.com/docker/docker/master/hack/dind \
    && chmod +x /dind

# Install bats
RUN cd /usr/local/src/ \
    && git clone https://github.com/sstephenson/bats.git \
    && cd bats \
    && ./install.sh /usr/local

# Install docker-compose
RUN curl -L https://github.com/docker/compose/releases/download/1.3.3/docker-compose-`uname -s`-`uname -m` > /usr/local/bin/docker-compose \
    && chmod +x /usr/local/bin/docker-compose

ENTRYPOINT ["/dind"]
