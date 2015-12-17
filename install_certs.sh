#!/bin/sh
set -e

hostname=$1
if [ "$hostname" = "" ]; then
	hostname="localhost"
fi

if [ "$hostname" != "localhost" ]; then
	IP=$(ifconfig eth0|grep "inet addr:"| cut -d: -f2 | awk '{ print $1}')
	echo "$IP $hostname" >> /etc/hosts
	# TODO: Check if record already exists in /etc/hosts
fi

install_ca() {
	mkdir -p $1/$hostname:$2
	cp ./nginx/ssl/registry-ca+ca.pem $1/$hostname:$2/ca.crt
	if [ "$3" != "" ]; then
		cp ./nginx/ssl/registry-$3+client-cert.pem $1/$hostname:$2/client.cert
		cp ./nginx/ssl/registry-$3+client-key.pem $1/$hostname:$2/client.key
	fi
}

install_test_certs() {
	install_ca $1 5011
	install_ca $1 5440
	install_ca $1 5441
	install_ca $1 5442 ca
	install_ca $1 5443 noca
	install_ca $1 5444 ca
	install_ca $1 5447 ca
	# For test remove CA
	rm $1/${hostname}:5447/ca.crt
	install_ca $1 5448
}

install_test_certs /etc/docker/certs.d
install_test_certs /root/.docker/tls

# Notary server
mkdir -p /root/.docker/tls/$hostname:4443
cp ./notary-server/certs/ca.pem /root/.docker/tls/${hostname}:4443/ca.crt
cp ./notary-server/certs/${hostname}.key /root/.docker/tls/${hostname}:4443/${hostname}.key
cp ./notary-server/certs/${hostname}.cert /root/.docker/tls/${hostname}:4443/${hostname}.cert

# Malevolent server
mkdir -p /etc/docker/certs.d/$hostname:6666
cp ./malevolent/certs/ca.pem /etc/docker/certs.d/$hostname:6666/ca.crt
