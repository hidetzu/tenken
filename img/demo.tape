# README のデモ（img/demo.gif）の台本。直接は使わない: img/record-demo.sh が {{PORT}} と {{BIN}} を埋めてから vhs に渡す。
Output img/demo.gif

Set Shell bash
Set FontSize 13
Set Width 1500
Set Height 640
Set Padding 16
Set TypingSpeed 40ms

Hide
Type `export PATH='{{BIN}}':"$PATH" PS1='$ '` Enter
Type "clear" Enter
Show

Type "tenken --no-external http://127.0.0.1:{{PORT}}/"
Sleep 500ms
Enter
Sleep 5s
