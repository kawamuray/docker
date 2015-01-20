#!/bin/bash
set -e

clear_page_cache() {
    echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null
}

execbm() {
    local id=$(docker $*)
    echo -e "GET /\r\n\r" | ./connect-bm $(docker inspect --format='{{ .NetworkSettings.IPAddress }}' $id):8080
    docker stop $id >/dev/null
    docker rm $id >/dev/null
}

echo "Regular startup with 'docker run'"
clear_page_cache
execbm run -d tomcat

echo "Startup with 'docker start'"
id=$(docker run -d tomcat); sleep 3
docker stop $id >/dev/null
clear_page_cache
execbm start $id

echo "Startup with 'docker restore'"
id=$(docker run -d tomcat); sleep 3
docker checkpoint --stop $id >/dev/null
clear_page_cache
execbm restore $id $(docker inspect --format='{{with index .Checkpoints 0}}{{ .ID }}{{ end }}' $id)

echo "Startup with 'docker restore' with page cache"
id=$(docker run -d tomcat); sleep 3
docker checkpoint --stop $id >/dev/null
execbm restore $id $(docker inspect --format='{{with index .Checkpoints 0}}{{ .ID }}{{ end }}' $id)

echo "Startup with 'docker restore --clone'"
id=$(docker run -d tomcat); sleep 3
docker checkpoint --stop $id >/dev/null
clear_page_cache
execbm restore --clone $id $(docker inspect --format='{{with index .Checkpoints 0}}{{ .ID }}{{ end }}' $id)

echo "Startup with 'docker restore --clone' with page cache"
id=$(docker run -d tomcat); sleep 3
docker checkpoint --stop $id >/dev/null
execbm restore --clone $id $(docker inspect --format='{{with index .Checkpoints 0}}{{ .ID }}{{ end }}' $id)

# echo "Startup with 'docker restore' with tmpfs"
# id=$(docker run -d tomcat); sleep 3
# sudo mkdir /var/lib/docker/containers/$id/checkpoints
# sudo mount -t tmpfs -o size=512M tmpfs /var/lib/docker/checkpoints
# cleanup() {
#     sudo umount /var/lib/docker/checkpoints
# }
# trap cleanup EXIT
# docker checkpoint --stop $id >/dev/null
# clear_page_cache
# execbm restore $id $(docker inspect --format='{{with index .Checkpoints 0}}{{ .ID }}{{ end }}' $id)
