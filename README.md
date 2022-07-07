# Ingress

## Service
1. Connect to message bus
2. Launch rtmp server
3. Wait for requests

## RTMP
1. Ingestion endpoint request sent from Server SDK -> LiveKit Server
2. LiveKit Server posts request using RPC client
3. Ingress instance accepts request and returns generated endpoint
4. Ingress instance prepares gstreamer pipeline and joins room
   1. Gstreamer rtmp2src -> decode -> encode -> appsink
   2. Appsink sends data to LiveKit Server using Go SDK
5. Once data is received, set pipeline to playing
6. Stream ends on StopIngress or EOS
