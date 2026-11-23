# A guest short of CPU until the host ends it, for TestAnAdoptedMachinesPressureIsHeard.
set -u
# It starts with the boot's rest, after multi-user.target's first units.
for i in $(seq 200); do
    systemctl is-active --quiet spin-machine-pressure.service && break
    sleep 0.1
done
systemctl is-active --quiet spin-machine-pressure.service || { echo PRESSURE_CHECK_FAILED: the sender is not running; exit 1; }
# Three runnable loops on one vCPU: a guest CPU stall each second, and a host one under cpu.max.
echo PHASE load
i=0; while [ "$i" -lt 3 ]; do timeout 240 sh -c 'while :; do :; done' & i=$((i + 1)); done; wait
