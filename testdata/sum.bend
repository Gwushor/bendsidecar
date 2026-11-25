import Base

# A kernel with one answer: three numbers, printed as text.
def main() -> IO(Unit):
  do IO<Unit>:
    IO.print("42 43 44")
