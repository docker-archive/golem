FROM distribution/golem-runner:base

# Install bats
RUN mkdir /usr/local/src && cd /usr/local/src/ \
    && git clone https://github.com/sstephenson/bats.git \
    && cd bats \
    && ./install.sh /usr/local


