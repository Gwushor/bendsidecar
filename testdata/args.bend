import Base

# How many arguments the OS handed the program: the one thing the sidecar contract
# depends on.
def count(args: List<String>, +acc: Nat) -> Nat:
  match args:
    case Nil{}:
      acc
    case Con{h, t}:
      count(t, Nat.add(acc, 1n))

def main() -> IO(Unit):
  do IO<Unit>:
    args : List<String> <- IO.args()
    IO.print(Nat.show(count(args, 0n)))
