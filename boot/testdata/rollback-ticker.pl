# The process a rollback must bring back as it was (boot/rollback_test.go): 4 MiB of random bytes
# held in memory and a counter, a line every 100 ms with both of the guest's clocks, to its stdout
# and to the serial console, where the host stamps when each one arrives.
#
#   SIGUSR1  writes "PID COUNTER SHA256-OF-THE-BYTES" to /run/ticker.state
#   SIGUSR2  draws new bytes, as a workspace changing its memory after a checkpoint
use strict;
use warnings;
use Digest::SHA qw(sha256_hex);
use Time::HiRes qw(clock_gettime CLOCK_REALTIME CLOCK_MONOTONIC sleep);

open(my $random, '<', '/dev/urandom') or die "/dev/urandom: $!";
my $bytes;
sub draw { read($random, $bytes, 4 << 20) == 4 << 20 or die "/dev/urandom: short read" }
draw();
my $n = 0;
$SIG{USR1} = sub {
	open(my $f, '>', '/run/ticker.state.new') or die "/run/ticker.state.new: $!";
	print $f "$$ $n ", sha256_hex($bytes), "\n";
	close($f) or die "/run/ticker.state.new: $!";
	rename('/run/ticker.state.new', '/run/ticker.state') or die "/run/ticker.state: $!";
};
$SIG{USR2} = \&draw;
# The console is opened again when a write to it fails: the serial getty's agetty hangs the tty up
# as it starts (vhangup), and a descriptor opened before then writes nothing from then on.
my $console;
sub console {
	open($console, '>', '/dev/ttyS0') or return 0;
	$console->autoflush(1);
	return 1;
}
STDOUT->autoflush(1);
while (1) {
	$n++;
	my $line = sprintf("%d %.6f %.6f\n", $n, clock_gettime(CLOCK_REALTIME), clock_gettime(CLOCK_MONOTONIC));
	print $line;
	unless ($console && print $console "TICK $line") {
		print $console "TICK $line" if console();
	}
	sleep(0.1);
}
