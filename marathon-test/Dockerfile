# Dockerfile to test the RBD Ceph Plugin via a marathon json job

# container expects a mounted file, appends data to it and then exits after short sleep.

# JSON file to lauch this job: marathon-test.json
#    export MARATHON_HOST=localhost
#    curl -X POST -H "Content-Type: application/json" \
#        http://${MARATHON_HOST}:8080/v2/apps \
#        -d@marathon-test.json

FROM gliderlabs/alpine:edge

WORKDIR /root

ENV RBD_TEST /mnt/foo/
ADD run.sh /root/run.sh
CMD ["/root/run.sh"]
