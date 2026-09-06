# Passing a GPU to a bhyve guest

The host and the guest cannot both drive a graphics card. Passing one through
means the host gives it up until you undo these steps, and giving it up takes
a reboot: the card is claimed by its driver early in boot, and nothing
releases it afterwards.

That is why this example ships a procedure rather than a manifest alone.

## 1. Find the card

```sh
pciconf -lv | grep -B1 -i nvidia
```

```
vgapci0@pci0:1:0:0:  class=0x030000 ... 'GP106 [GeForce GTX 1060 6GB]'
hdac0@pci0:1:0:1:    class=0x040300 ... 'GP106 High Definition Audio Controller'
```

Two entries, not one: a modern card exposes its HDMI audio as a second
function. They share an IOMMU group, so they travel together — passing the
graphics function alone leaves the guest with a card it cannot fully drive.

`pci0:1:0:0` is written `1/0/0` below: bus, device, function.

## 2. Check the machine can do it

```sh
sysctl hw.vmm.vtd.enable        # 1 on Intel with VT-d
kldstat -v | grep ppt           # the passthrough driver
```

VT-d on Intel, or AMD-Vi, has to be enabled in firmware. Without an IOMMU the
kernel cannot contain the device's DMA, and bhyve will not pass it.

## 3. Give the card to the ppt driver

In `/boot/loader.conf`:

```
vmm_load="YES"
ppt_load="YES"
hw.vmm.vtd.enable=1

# Hand the GTX 1060 and its HDMI audio to the passthrough driver.
pptdevs="1/0/0 1/0/1"
```

Then reboot. This is the step that costs you the card: afterwards the host has
no driver for it, so do not do this on a machine whose only console is that
GPU.

Confirm the handover:

```sh
pciconf -lv | grep '^ppt'
```

```
ppt0@pci0:1:0:0:  class=0x030000 ...
ppt1@pci0:1:0:1:  class=0x040300 ...
```

The entries now read `ppt0`/`ppt1` instead of `vgapci0`/`hdac0`. While they do
not, the host driver still holds the card and the VM cannot start.

## 4. Run the VM

```sh
hospitus image fetch cloud:debian-12-amd64
doas hospitus apply --start --var name=gpu template.toml
```

Override the addresses if yours differ:

```sh
doas hospitus apply --start --var name=gpu \
    --var gpu_pci=2/0/0 --var gpu_audio_pci=2/0/1 template.toml
```

## 5. Check the guest actually got it

```sh
ssh -p 2220 debian@<host-ip>
lspci -nn | grep -i nvidia
```

This works only once you have put your own public key into the `debian` user's
`ssh_authorized_keys` in `template.toml` and applied it: the manifest sets
`lock_passwd = true`, so there is no password to fall back on. Without a key,
use the VNC console.

The card should appear with its real vendor and device IDs. The manifest runs
that same command through cloud-init on first boot, so
`/var/log/cloud-init-output.log` holds the answer even if you cannot log in.

Seeing the device is not the same as being able to use it. Installing a vendor
driver in the guest is a separate exercise, and on Debian the NVIDIA driver
lives in the non-free repository.

## Giving the card back

Remove the `pptdevs` line from `/boot/loader.conf` and reboot. The host driver
claims the card again on the next boot.

## When it does not work

**The VM will not start and bhyve complains about the device.** The card is
still bound to its host driver. Re-check step 3: `pciconf -lv` has to show
`ppt0`, not `vgapci0`.

**The guest sees the card but the display stays dark.** Expected without a
driver in the guest. Check `lspci` first — a card that does not appear at all
is a passthrough problem, one that appears but drives no screen is a driver
problem.

**Only the graphics function was passed.** The audio function stayed with the
host, and the guest may fail to initialize the card. Pass both.
