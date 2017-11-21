#!/bin/bash

# cron sript to determine whether mesos agent node has correct Ceph and RBD
# configuration needed for rbd-docker-plugin

CEPH_CONF=${CEPH_CONF:-/etc/ceph/ceph.conf}
CEPH_USER=${CEPH_USER:-admin}

CEPH_KEY=${CEPH_KEY:-"/etc/ceph/ceph.client.${CEPH_USER}.keyring"}

if [ ! -f "$CEPH_CONF" ]; then
    echo "$0: $HOSTNAME: ERROR missing CEPH_CONF: $CEPH_CONF"
    exit 1
fi

if [ ! -f "$CEPH_KEY" ]; then
    echo "$0: $HOSTNAME: ERROR missing expected keyring for CEPH_USER=$CEPH_USER: $CEPH_KEY"
    exit 3
fi

# try running rbd command
RBD_TEST=$(rbd --conf ${CEPH_CONF} --id ${CEPH_USER} ls 2>&1)
if [ $? != 0 ]; then
    echo "$0: $HOSTNAME: ERROR running rbd command: $RBD_TEST"
    exit 5
fi
