# LiveKit Ingress

WebRTC is proving to be a versatile and scalable transport protocol both for media ingestion and delivery. However, some applications may require integrating with existing workflows or equipment that do not support WebRTC. Universal ingress provides a way to send media that was generated using such workflows to a LiveKit room. 

## Capabilities

Universal Ingress is meant to be a versatile service supporting a variety of protocols, both using a push and pull model. Currently, the following protcols are supported:
- RTMP

Once an ingress has been created, it will join the associated room and forward media to is as soon as a source sends media to the associated endpoint. 

LiveKit's ingress service will automatically transcode streams for you using GStreamer.

## Supported Output

The ingress serice will transcode the source media to ensure compatibility with WebRTC. Simulcast is supported. The parameters of the different video layers can be defined at ingress creation time. 

## Documentation

[Upcoming]

### Service Architecture

The ingress service and the livekit server communicate over redis. Redis is also used as storage for the ingress session state. The ingress service must also expose a public IP address for the publishing endpoint streamers will connect to. In a typical cluster setup, this IP address would be assigned to a load balancer that would forward incoming connection to an available ingress service instance. The targeted ingress instance will then validate the incoming request with the livekit server using redis as RPC transport. 

### Config

The Ingress service takes a yaml config file:

```yaml
# required fields
api_key: livekit server api key. LIVEKIT_API_KEY env can be used instead
api_secret: livekit server api secret. LIVEKIT_API_SECRET env can be used instead
ws_url: livekit server websocket url. LIVEKIT_WS_URL can be used instead
redis:
  address: must be the same redis address used by your livekit server
  username: redis username
  password: redis password
  db: redis db

# optional fields
health_port: if used, will open an http port for health checks
prometheus_port: port used to collect prometheus metrics. Used for autoscaling
log_level: debug, info, warn, or error (default info)
rtmp_port: port to listen to incoming RTMP connection on (default 1935)
http_relay_port: port used to relay data from the main service process to the per ingress handler process (default 9090)

# cpu costs for various ingress types with their default values
cpu_cost:
  rtmp_cpu_cost: 2.0
```

The config file can be added to a mounted volume with its location passed in the INGRESS_CONFIG_FILE env var, or its body can be passed in the INGRESS_CONFIG_BODY env var.

In order for the livekit server to be able to create ingress sessions, an `ingress` section must also be added to the livekit-server configuration:

```yaml
ingress:
  rtmp_base_url: rtmp url prefix pointing to the ingress external IP address or load balancer
```

For instance:
```yaml
ingress:
  rtmp_base_url: rtmp://my.domain.com/x
```

A stream key will be appended to this prefix to generate the ingress session specific rtmp publishing endpoint.

### Using the ingress service

#### RTMP

The first step in order to use the ingress service is to create an ingress session and associate it with a room. This can be done with any of the server SDKs or with the [livekit-cli](https://github.com/livekit/livekit-cli). The syntax with the livekit-cli is as follow:

`livekit-cli create-ingress --url <livekit server websocket url> --api-key <livekit server api key> --api-secret <livekit server api secret> --request <path to ingress creation request JSON file>`

The request creation JSON file uses the following syntax:

```json
{
    "name": Name of the Ingress,
    "room_name": Name of the room to connect to,
    "participant_identity": Unique identity for the room participant the ingress service will connect as,
    "participant_name": Name displayed in the room for the participant
}
```

On success, livekit-cli will return the unique id for the ingress. 

It is possible to get details on all created ingress with the `list-ingress` command:

`livekit-cli list-ingress --url wss://encom.staging.livekit.cloud --api-key <livekit server api key> --api-secret <livekit server api secret>`

In particilar, this will return the RTMP url to use to setup the RTMP encoder. 

### Running locally

#### Running natively

The Ingress service can be run natively on any platform supported by GStreamer.

##### Prequisistes

The Ingress service is built in Go. Go >= v1.17 is needed. The following [GStreamer](https://gstreamer.freedesktop.org/) libraries and headers must be installed:
- gstreamer
- gst-plugins-base
- gst-plugins-good
- gst-plugins-bad
- gst-plugins-ugly
- gst-libav

On MacOS, these can be installed using [Homebrew](https://brew.sh/) by running `mage bootstrap`. 

##### Building

Build the Ingress service by running:

`mage build`

##### Running the service

To run against a local livekit server, a redis server must be running locally. All servers must be configured to communicate over localhost. Create a file named `config.yaml` with the following content:

```yaml
log_level: debug
api_key: <your-api-key>
api_secret: <your-api-secret>
ws_url: ws://localhost:7880
redis:
  address: localhost:6379
```

On MacOS, if GStreamer was installed using Homwbrew, the following environment must be set:
```shell
export GST_PLUGIN_PATH=/opt/homebrew/Cellar/gst-plugins-base:/opt/homebrew/Cellar/gst-plugins-good:/opt/homebrew/Cellar/gst-plugins-bad:/opt/homebrew/Cellar/gst-plugins-ugly:/opt/homebrew/Cellar/gst-plugins-bad:/opt/homebrew/Cellar/gst-libav 
```

Then to run the service:

```shell
ingress --config=config.yaml
```

#### Running with Docker

To run against a local livekit server, a redis server must be running locally. The Ingress service must be instructed to connect to livekit server and redis on the host. The host network is accessible from within the container on IP:
- 192.168.65.2 on MacOS and Windows
- 172.17.0.1 on linux

Create a file named `config.yaml` with the following content:

```yaml
log_level: debug
api_key: <your-api-key>
api_secret: <your-api-secret>
ws_url: ws://192.168.65.2:7880 (or ws://172.17.0.1:7880 on linux)
redis:
  address: 192.168.65.2:6379 (or 172.17.0.1:6379 on linux)
```

Then to run the service:

```shell
docker run --rm \
    -e INGRESS_CONFIG_BODY=`cat config.yaml` \
    -p 1935:1935 \
    livekit/ingress
```

