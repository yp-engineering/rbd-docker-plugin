

all: build

build:
	rpmbuild -ba ~/rpmbuild/SPECS/rbd-docker-plugin.spec

env:
	rpmdev-setuptree
	cp -Rvf S* ~/rpmbuild
	wget -O ~/rpmbuild/SOURCES/0.1.9.tar.gz https://github.com/yp-engineering/rbd-docker-plugin/archive/0.1.9.tar.gz
	wget -O ~/rpmbuild/SOURCES/rbd-docker-plugin_logrotate https://github.com/yp-engineering/rbd-docker-plugin/blob/master/logrotate.d/rbd-docker-plugin_logrotate

depends:
	yum install -y http://ceph.com/rpm-infernalis/el7/noarch/ceph-release-1-1.el7.noarch.rpm epel-release
	yum install -y librados2-devel librbd1-devel golang git epel-release rpmdevtools make wget
