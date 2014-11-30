#sudo cp -Ra mkroot/rootfs/ /var/lib/rkt/nspawn
ln -s /usr/lib64 /var/lib/rkt/nspawn/lib64

mkdir -p /var/lib/rkt/nspawn/var
mount -o bind /var /var/lib/rkt/nspawn/var

mkdir -p /var/lib/rkt/nspawn/tmp
mount -o bind /tmp /var/lib/rkt/nspawn/tmp

mkdir -p /var/lib/rkt/nspawn/proc
mount -o bind /proc /var/lib/rkt/nspawn/proc

mkdir -p /var/lib/rkt/nspawn/dev
mount -o bind /dev /var/lib/rkt/nspawn/dev

mount -o bind /dev/pts /var/lib/rkt/nspawn/dev/pts

mkdir -p /var/lib/rkt/nspawn/run
mount -o bind /run /var/lib/rkt/nspawn/run

mkdir -p /var/lib/rkt/nspawn/sys
mount -o bind /sys /var/lib/rkt/nspawn/sys
