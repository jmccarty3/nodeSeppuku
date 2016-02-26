FROM alpine

ADD nodeSeppuku /

ENTRYPOINT ["/nodeSeppuku"]
