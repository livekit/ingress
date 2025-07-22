<!--BEGIN_BANNER_IMAGE-->

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="/.github/banner_dark.png">
  <source media="(prefers-color-scheme: light)" srcset="/.github/banner_light.png">
  <img style="width:100%;" alt="The LiveKit icon, the name of the repository and some sample code in the background." src="https://raw.githubusercontent.com/livekit/ingress/main/.github/banner_light.png">
</picture>

<!--END_BANNER_IMAGE-->

# LiveKit Ingress

<!--BEGIN_DESCRIPTION-->
WebRTC is proving to be a versatile and scalable transport protocol both for media ingestion and delivery. However, some applications may require integrating with existing workflows or equipment that do not support WebRTC. Universal Ingress provides a way to send media that was generated using such workflows to a LiveKit room.
<!--END_DESCRIPTION-->

## Capabilities

Universal Ingress is meant to be a versatile service supporting a variety of protocols, both using a push and pull model. Currently, the following protcols are supported:
- RTMP
- WHIP (https://datatracker.ietf.org/doc/draft-ietf-wish-whip/)

## Supported Output

The Ingress service will automatically transcode the source media to ensure compatibility with WebRTC. It can publish multiple layers with [Simulcast](https://blog.livekit.io/an-introduction-to-webrtc-simulcast-6c5f1f6402eb/). The parameters of the different video layers can be defined at ingress creation time. 

## Documentation

### Push workflow

To push media to the Ingress, the workflow goes like this:

* create an Ingress with `CreateIngress` API (to livekit-server)
* `CreateIngress` returns a URL that can be used to push media to
* copy and paste the URL into your streaming workflow
* start the stream
* Ingress starts receiving data
* Ingress joins the LiveKit room and publishes transcoded media

### Service Architecture

The Ingress service and the LiveKit server communicate over Redis. Redis is also used as storage for the Ingress session state. The Ingress service must also expose a public IP address for the publishing endpoint streamers will connect to. In a typical cluster setup, this IP address would be assigned to a load balancer that would forward incoming connection to an available Ingress service instance. The targeted Ingress instance will then validate the incoming request with the LiveKit server using Redis as RPC transport. 

### Config

The Ingress service takes a YAML config file:

```yaml
# required fields
api_key: livekit server api key. LIVEKIT_API_KEY env can be used instead
api_secret: livekit server api secret. LIVEKIT_API_SECRET env can be used instead
ws_url: livekit server websocket url. LIVEKIT_WS_URL env can be used instead
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
whip_port: port to listen to incoming WHIP calls on (default 8080)
http_relay_port: port used to relay data from the main service process to the per ingress handler process (default 9090)
rtc_config: configuration for ICE and other RTC related settings, same settings livekit-server RTC configuration. Used for WHIP.

# cpu costs for various Ingress types with their default values
cpu_cost:
  rtmp_cpu_cost: 2.0
  whip_cpu_cost: 2.0
```

The config file can be added to a mounted volume with its location passed in the INGRESS_CONFIG_FILE env var, or its body can be passed in the INGRESS_CONFIG_BODY env var.

In order for the LiveKit server to be able to create Ingress sessions, an `ingress` section must also be added to the livekit-server configuration:

```yaml
ingress:
  rtmp_base_url: RTMP url prefix pointing to the Ingress external IP address or load balancer
  whip_base_url: WHIP url prefix pointing to the Ingress external IP address or load balancer
```

For instance:
```yaml
ingress:
  rtmp_base_url: rtmp://my.domain.com/x
  whip_base_url: http://my.domain.com/w
```

A stream key will be appended to this prefix to generate the ingress session specific RTMP or WHIP publishing endpoint.

### Using the Ingress service

#### RTMP and WHIP

The first step in order to use the Ingress service is to create an ingress session and associate it with a room. This can be done with any of the server SDKs or with the [livekit-cli](https://github.com/livekit/livekit-cli). The syntax with the livekit-cli is as follow:

```shell
livekit-cli create-ingress \
  --request <path to Ingress creation request JSON file>
```

The request creation JSON file uses the following syntax:

```json
{
    "input_type": 0 for RTMP, 1 for WHIP
    "name": Name of the Ingress,
    "room_name": Name of the room to connect to,
    "participant_identity": Unique identity for the room participant the Ingress service will connect as,
    "participant_name": Name displayed in the room for the participant
}
```

On success, `livekit-cli` will return the unique id for the Ingress. 

It is possible to get details on all created Ingress with the `list-ingress` command:

```shell
livekit-cli list-ingress
```

In particular, this will return the RTMP url WHIP endpoint to use to setup the encoder. 

### Running locally

#### Running natively

The Ingress service can be run natively on any platform supported by GStreamer.

##### Prerequisites

The Ingress service is built in Go. Go >= 1.18 is needed. The following [GStreamer](https://gstreamer.freedesktop.org/) libraries and headers must be installed:
- gstreamer
- gst-plugins-base
- gst-plugins-good
- gst-plugins-bad
- gst-plugins-ugly
- gst-libav

On MacOS, these can be installed using [Homebrew](https://brew.sh/) by running `mage bootstrap`. 

##### Building

Build the Ingress service by running:

```shell
mage build
````

##### Running the service

To run against a local LiveKit server, a redis server must be running locally. All servers must be configured to communicate over localhost. Create a file named `config.yaml` with the following content:

```yaml
log_level: debug
api_key: <your-api-key>
api_secret: <your-api-secret>
ws_url: ws://localhost:7880
redis:
  address: localhost:6379
```

On MacOS, if GStreamer was installed using Homebrew, the following environment must be set in order to ensure that GStreamer can load all its plugins:

```shell
export DYLD_LIBRARY_PATH=/opt/homebrew/lib
```

Then to run the service:

```shell
ingress --config=config.yaml
```

If starting an Ingress fails with a GStreamer error such as `Failed to load libsoup library` or `no such element factory "souphttpsrc`, try deleting your GStreamer plugin registry with:

```shell 
rm -rf ~/.cache/gstreamer-1.0/
```

#### Running with Docker

To run against a local LiveKit server, a Redis server must be running locally. The Ingress service must be instructed to connect to LiveKit server and Redis on the host. The host network is accessible from within the container on IP:
- host.docker.internal on MacOS and Windows
- 172.17.0.1 on linux

Create a file named `config.yaml` with the following content:

```yaml
log_level: debug
api_key: <your-api-key>
api_secret: <your-api-secret>
ws_url: ws://host.docker.internal:7880 (or ws://172.17.0.1:7880 on linux)
redis:
  address: host.docker.internal:6379 (or 172.17.0.1:6379 on linux)
```

In order to be able to use establish WHIP sessions over UDP, the container must be run with host networking enabled. 

Then to run the service:

```shell
docker run --rm \
    -e INGRESS_CONFIG_BODY="`cat config.yaml`" \
    -p 1935:1935 \
    -p 8080:8080 \
    --network host \
    livekit/ingress
```

<!--BEGIN_REPO_NAV-->
<br/><table>
<thead><tr><th colspan="2">LiveKit Ecosystem</th></tr></thead>
<tbody>
<tr><td>LiveKit SDKs</td><td><a href="https://github.com/livekit/client-sdk-js">Browser</a> · <a href="https://github.com/livekit/client-sdk-swift">iOS/macOS/visionOS</a> · <a href="https://github.com/livekit/client-sdk-android">Android</a> · <a href="https://github.com/livekit/client-sdk-flutter">Flutter</a> · <a href="https://github.com/livekit/client-sdk-react-native">React Native</a> · <a href="https://github.com/livekit/rust-sdks">Rust</a> · <a href="https://github.com/livekit/node-sdks">Node.js</a> · <a href="https://github.com/livekit/python-sdks">Python</a> · <a href="https://github.com/livekit/client-sdk-unity">Unity</a> · <a href="https://github.com/livekit/client-sdk-unity-web">Unity (WebGL)</a> · <a href="https://github.com/livekit/client-sdk-esp32">ESP32</a></td></tr><tr></tr>
<tr><td>Server APIs</td><td><a href="https://github.com/livekit/node-sdks">Node.js</a> · <a href="https://github.com/livekit/server-sdk-go">Golang</a> · <a href="https://github.com/livekit/server-sdk-ruby">Ruby</a> · <a href="https://github.com/livekit/server-sdk-kotlin">Java/Kotlin</a> · <a href="https://github.com/livekit/python-sdks">Python</a> · <a href="https://github.com/livekit/rust-sdks">Rust</a> · <a href="https://github.com/agence104/livekit-server-sdk-php">PHP (community)</a> · <a href="https://github.com/pabloFuente/livekit-server-sdk-dotnet">.NET (community)</a></td></tr><tr></tr>
<tr><td>UI Components</td><td><a href="https://github.com/livekit/components-js">React</a> · <a href="https://github.com/livekit/components-android">Android Compose</a> · <a href="https://github.com/livekit/components-swift">SwiftUI</a> · <a href="https://github.com/livekit/components-flutter">Flutter</a></td></tr><tr></tr>
<tr><td>Agents Frameworks</td><td><a href="https://github.com/livekit/agents">Python</a> · <a href="https://github.com/livekit/agents-js">Node.js</a> · <a href="https://github.com/livekit/agent-playground">Playground</a></td></tr><tr></tr>
<tr><td>Services</td><td><a href="https://github.com/livekit/livekit">LiveKit server</a> · <a href="https://github.com/livekit/egress">Egress</a> · <b>Ingress</b> · <a href="https://github.com/livekit/sip">SIP</a></td></tr><tr></tr>
<tr><td>Resources</td><td><a href="https://docs.livekit.io">Docs</a> · <a href="https://github.com/livekit-examples">Example apps</a> · <a href="https://livekit.io/cloud">Cloud</a> · <a href="https://docs.livekit.io/home/self-hosting/deployment">Self-hosting</a> · <a href="https://github.com/livekit/livekit-cli">CLI</a></td></tr>
</tbody>
</table>
<!--END_REPO_NAV-->
