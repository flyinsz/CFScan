package main

import "fmt"

const bannerLogo = "ＣＦＤＡＴＡ-ＷＥＢ"
const bannerAuthor = "by:GitHub/PoemMisty"

func printBanner() {
	fmt.Println()
	fmt.Println(colorize(bannerLogo, ansiBold+ansiGreen))
	fmt.Println(bannerAuthor)
	fmt.Println()
}
