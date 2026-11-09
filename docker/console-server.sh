# Portainer's console clears the screen right after connecting, so show the help again after that clear.
echo() { command echo "$@"; case "$*" in *2J*) tinnie-server help ;; esac; }
tinnie-server help
