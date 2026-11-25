import Base

# A data-dependent loop: the multiplier chain cannot collapse into a closed form,
# so the work is real and a timeout actually fires.
def spin(n: Nat, +acc: U32) -> U32:
  match n:
    case 0n:
      acc
    case 1n+p:
      spin(p, (acc * 16777619 + 1 : U32))

def main() -> IO(Unit):
  do IO<Unit>:
    IO.print(U32.show(spin(2000000000n, 1)))
