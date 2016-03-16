#!/bin/bash

# append a hello to a log file

RBD_TEST=${RBD_TEST:-/mnt/foo}
LOG_FILE=${RBD_TEST}/hello.log


# don't make marathon churn too much ...
SLEEP_TIME=${SLEEP_TIME:-300}

# check for the file
LOG_ERROR=0
if [ ! -f $LOG_FILE ] ; then
    echo "ERROR: unable to find log file: $LOG_FILE"
    LOG_ERROR=1
else
    echo "NOTE: found the existing mounted log file: $LOG_FILE ==>"
        cat $LOG_FILE
    echo "----"
fi

# append our note
echo "hello from $HOSTNAME on `date`" | tee -a $LOG_FILE

# sleep a bit and exit
echo -n "sleeping $SLEEP_TIME ... "
sleep $SLEEP_TIME

echo "goodbye from $HOSTNAME"

if [ $LOG_ERROR != 0 ] ; then
    exit $LOG_ERROR
fi
