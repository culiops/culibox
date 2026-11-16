/* abduco includes <util.h> for forkpty on macOS. zig's macOS headers have no
 * util.h, while its libSystem stub does export forkpty, so this is the one
 * declaration abduco needs, as in the macOS SDK's util.h. Used only by
 * scripts/release/build-abduco.sh for the darwin builds. */
#include <sys/ioctl.h>
#include <termios.h>

int forkpty(int *, char *, struct termios *, struct winsize *);
