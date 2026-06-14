with open("server.py", "r") as f:
    lines = f.readlines()

with open("server.py", "w") as f:
    for line in lines:
        if line == "        f.write('\n":
            f.write("        f.write('\\n')\n")
        elif line == "')\n":
            pass
        else:
            f.write(line)
