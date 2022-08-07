# Automated Wake-on-Lan for Caddy

`caddy-wol` is a Caddy plugin that sends wake-on-lan magic packets to remote hosts to wake up e.g. reverse proxy targets when they can not be reached.

## Installation

To add this module to your Caddy configuration, run the following [xcaddy](https://caddyserver.com/docs/build#xcaddy) command:

```shell
xcaddy build \
    --with github.com/dulli/caddy-wol
```

## Usage

The plugin implements a minimal HTTP handler directive that simply dispatches a WOL magic packet and then passes the request through unchanged to the next handler which may return a response to the client:

```
wake_on_lan <mac_address>
```

As with any such directive, you have to tell Caddy where to put in the execution order of all directives. This is done with a global configuration directive in your Caddyfile:

```
order wake_on_lan before respond
```

Internally, the magic packets are throttled to be sent at most once every 10 minutes per remote host, even if more frequent requests arrive at the server, as sending the packet once is of course enough to wake up the remote host.

## Example Configuration

One possible example use case is to reverse proxy requests to e.g. a NAS (using the IP `192.168.0.100` and MAC address `00:11:22:33:44:55`) on which a media server like Jellyfin is running (at port `:8096`). If the NAS is shut down when not in use to save energy, we want it to automatically boot up again, when somebody needs it again.

To do so, we first issue a standard reverse proxy directive and then register an error handler that is called when this proxy request goes unanswered because the target is not reachable. It then calls the `caddy-wol` plugin to wake up the NAS and re-issues the reverse proxy directive, but this time with a longer timeout to allow for the boot process to finish. The configuration to do all of this, is really straight forward using built-in Caddy directives:

```
{
    order wake_on_lan before respond
}

wol.example.com {
        reverse_proxy 192.168.0.100:8096
        handle_errors {
                @502 expression {err.status_code} == 502
                handle @502 {
                        wake_on_lan 00:11:22:33:44:55
                        reverse_proxy 192.168.0.100:8096 {
                                lb_try_duration 120s
                        }
                }
        }
}
```

For the client, this results in an as nice as possible user experience, where the page load simply takes a longer time and then automatically shows the requested resource as soon as it is available (if the wait time stays in the bounds of the clients request timeout). No error messages have to be presented and no manual or automatic page refreshes are required.
