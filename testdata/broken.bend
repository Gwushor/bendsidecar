import Base

# Deliberately broken: the type checker must reject it, so Open reports KindBuild.
def main() -> IO(Unit):
  do IO<Unit>:
    IO.print(1 + true)
