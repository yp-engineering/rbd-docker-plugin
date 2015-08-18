FROM golang:1.4.2-wheezy

MAINTAINER Adam Avilla <aavilla@yp.com>

ENV SRC_ROOT /go/src/github.com/yp-engineering/rbd-docker-plugin
ENV CEPH_VERSION hammer

RUN curl -sSL 'https://ceph.com/git/?p=ceph.git;a=blob_plain;f=keys/release.asc' | \
    apt-key add -
RUN echo deb http://ceph.com/debian-${CEPH_VERSION}/ wheezy main | \
    tee /etc/apt/sources.list.d/ceph-${CEPH_VERSION}.list
RUN apt-get update && \
    apt-get install -y --force-yes librados-dev librbd-dev && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

RUN mkdir -p ${SRC_ROOT}
WORKDIR ${SRC_ROOT}

ADD . ${SRC_ROOT}

RUN go get -t .

CMD ["bash"]
