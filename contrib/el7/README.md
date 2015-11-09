# Centos7/RHEL7 contribs
## RPM
### Runtime dependencies
- docker from docker [RHEL repo](https://docs.docker.com/installation/rhel/)
- ceph >= 0.94.0
### Build the rpm
```
$ git clone https://github.com/yp-engineering/rbd-docker-plugin
$ cd rbd-docker-plugin/contrib/el7
# optional - install required rpms to build
$ sudo make depends
# mandatory
$ make env build
```
You can find your freshly build rpm in *~/rpmbuild/BUILDROOT/rbd-docker-plugin-0.1.9-1.el7.centos.x86_64*

