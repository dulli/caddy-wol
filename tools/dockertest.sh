name=caddy-wol-test

sudo sysctl -w net.ipv4.icmp_echo_ignore_broadcasts=0
sudo sysctl -w net.ipv4.conf.all.bc_forwarding=1

docker network create \
    -o "net.ipv4.icmp_echo_ignore_broadcasts=0" \
    -o "net.ipv4.conf.all.bc_forwarding=1" ${name}

subnet=$(docker network inspect ${name} | jq --raw-output .[0].IPAM.Config[0].Subnet)
interface=$(ip route | grep "$subnet" | cut -d ' ' -f3)
sudo sysctl -w net.ipv4.conf.${interface}.bc_forwarding=1

docker run --rm -it -p 2023:2023/tcp --network ${name} --name ${name} caddywol:latest

docker network rm ${name}